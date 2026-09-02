#!/usr/bin/env python3
"""Compute the spike's numbers from the raw captures.

Every figure in README.md comes out of this script. Run it and you get the table;
change the data and the table changes. Nothing is typed in by hand.

    python3 analyse.py                 # the baseline table
    python3 analyse.py --zoo-runs      # Zoo run-to-run comparison, by concept
    python3 analyse.py --zoo-spec      # re-derive the Zoo API surface (network)
"""
import json
import os
import sys
import urllib.request

HERE = os.path.dirname(os.path.abspath(__file__))

# NEMA 17 is a published standard. These are the figures the geometry is scored
# against, and they are stated here rather than inferred from any model's output
# so that a model cannot grade its own homework.
#   NEMA 17 flange face  42.3 mm square
#   bolt circle          31.0 mm square pattern
#   pilot boss           22.0 mm diameter
#   shaft                 5.0 mm diameter
NEMA17 = {"face_mm": 42.3, "bolt_spacing_mm": 31.0, "pilot_boss_dia_mm": 22.0, "shaft_dia_mm": 5.0}


def footprint(part):
    s = part.get("size", {})
    return s.get("width"), s.get("depth"), s.get("height")


def baseline():
    d = json.load(open(os.path.join(HERE, "data", "forge-baseline-runs.json")))
    print(f"prompt: {d['prompt']}")
    print(f"model:  {d['model']}   runs: {len(d['runs'])}   (identical prompt, nothing varied)\n")

    rows, shapes_seen, plates, bosses = [], set(), [], []
    for r in d["runs"]:
        p = r["prototype"]
        parts = p["parts"]
        plate = next((x for x in parts if "plate" in x["name"].lower() or "base" in x["name"].lower()), None)
        boss = next((x for x in parts if "boss" in x["name"].lower()), None)
        ribs = [x for x in parts if "rib" in x["name"].lower()]
        w, dpt, h = footprint(plate) if plate else (None, None, None)
        # width/depth are the footprint, height is the thickness for a plate lying flat
        face = max(v for v in (w, dpt) if v is not None)
        thick = min(v for v in (w, dpt, h) if v is not None)
        bdia = round(boss["size"].get("radius", 0) * 2, 1) if boss else None
        rib_shape = "/".join(sorted({x["shape"] for x in ribs})) or "-"
        shapes_seen.update(x["shape"] for x in parts)
        plates.append((face, thick))
        if bdia:
            bosses.append(bdia)
        rows.append((r["run"], r["total_ms"], len(parts), face, thick, bdia, rib_shape))

    print(f"{'run':>3} {'ms':>6} {'parts':>5} {'plate face':>10} {'thick':>6} {'boss ⌀':>7}  rib modelled as")
    for run, ms, n, face, thick, bdia, rib in rows:
        print(f"{run:>3} {ms:>6} {n:>5} {face:>10} {thick:>6} {bdia if bdia else '-':>7}  {rib}")

    faces = sorted({p[0] for p in plates})
    thicks = sorted({p[1] for p in plates})
    print(f"\nspread across identical prompts")
    print(f"  plate face   {faces}  -> {min(faces)}–{max(faces)} mm "
          f"({(max(faces)-min(faces))/NEMA17['face_mm']*100:.0f}% of the {NEMA17['face_mm']} mm standard face)")
    print(f"  plate thick  {thicks}  -> {min(thicks)}–{max(thicks)} mm "
          f"({(max(thicks)-min(thicks))/min(thicks)*100:.0f}% spread)")
    print(f"  boss ⌀       {sorted(set(bosses))}  -> standard pilot boss is {NEMA17['pilot_boss_dia_mm']} mm")
    print(f"  primitives   {sorted(shapes_seen)}")

    print("\ndimensional agreement with the standard (any run, any dimension)")
    hit = [f for f in faces if abs(f - NEMA17["face_mm"]) <= 0.5]
    print(f"  plate face within ±0.5 mm of 42.3: {hit if hit else 'none of 4 runs'}")

    print("\nclaims made in `assumptions`, checked against the standard")
    for r in d["runs"]:
        for a in r["prototype"]["assumptions"]:
            low = a.lower()
            if "20.5" in low:
                print(f"  run {r['run']}: WRONG  \"{a}\"")
                print(f"           ±20.5 mm is a 41 mm pattern; NEMA 17 is {NEMA17['bolt_spacing_mm']} mm square")
            elif "31" in low and ("spacing" in low or "mm" in low):
                print(f"  run {r['run']}: correct  \"{a[:72]}…\"")

    print("\ninternal contradictions (the text and the emitted shape disagree)")
    found = False
    for r in d["runs"]:
        p = r["prototype"]
        text = " ".join(p["assumptions"]).lower()
        for part in p["parts"]:
            if "rib" not in part["name"].lower():
                continue
            if ("frust" in text or "conical" in text) and part["shape"] == "cylinder":
                print(f"  run {r['run']}: assumptions say conical frustum, {part['id']} emitted shape=cylinder")
                found = True
            if part["shape"] == "cylinder" and part["size"].get("radius") == 0:
                print(f"  run {r['run']}: {part['id']} is a cylinder with radius 0 — degenerate solid")
                found = True
    if not found:
        print("  none")


# Zoo's KCL names a parameter freely, so two runs of the same part use different
# identifiers for the same quantity. Comparing by name reports "everything
# changed" and hides the only thing worth knowing. These map the concepts.
ZOO_CONCEPTS = {
    "bolt square": ["motorHolePitch", "motorBoltSpacing"],
    "bolt clearance ⌀": ["motorHoleDiameter", "motorBoltDiameter"],
    "pilot clearance ⌀": ["pilotClearanceDiameter", "motorPilotClearance"],
    "plate size": ["plateSize", "baseWidth"],
    "plate thickness": ["plateThickness", "baseThickness"],
    "boss ⌀": ["bossOuterDiameter"],
    "rib length": ["ribLength"],
}


def zoo_runs():
    """Compare the Zoo generations by CONCEPT, not by identifier."""
    import re
    runs = []
    for name in sorted(os.listdir(os.path.join(HERE, "data"))):
        f = os.path.join(HERE, "data", name, "main.kcl")
        if name.startswith("zoo-run") and os.path.exists(f):
            src = open(f).read()
            runs.append((name, dict(re.findall(r"^(\w+)\s*=\s*(-?[\d.]+(?:mm|deg)?)\s*$", src, re.M)), src))
    if not runs:
        print("no zoo runs in data/")
        return
    print(f"{'concept':22}" + "".join(f"{n:>14}" for n, _, _ in runs) + "   stable?")
    for concept, aliases in ZOO_CONCEPTS.items():
        vals = []
        for _, params, _ in runs:
            v = next((params[a] for a in aliases if a in params), "-")
            vals.append(v)
        seen = {v for v in vals if v != "-"}
        print(f"{concept:22}" + "".join(f"{v:>14}" for v in vals) +
              ("   yes" if len(seen) == 1 else "   varies"))
    print()
    for n, params, src in runs:
        print(f"  {n}: {len(params)} named parameters, {src.count(chr(10))+1} lines")
    print("\nRead the split: the figures that come from the STANDARD hold across runs;")
    print("the figures Zoo CHOSE do not, and are not claimed to be standard.")


ZOO_SPEC = "https://raw.githubusercontent.com/KittyCAD/kittycad.go/main/spec.json"


def zoo_spec():
    """Re-derive Zoo's ML surface from the published OpenAPI document."""
    print(f"fetching {ZOO_SPEC}")
    with urllib.request.urlopen(ZOO_SPEC, timeout=60) as fh:
        spec = json.load(fh)
    print(f"Zoo API {spec['info'].get('version')}  (openapi {spec.get('openapi')})\n")
    out = []
    for path, item in sorted(spec["paths"].items()):
        if not any(k in path for k in ("/ml", "/ai", "cad")):
            continue
        methods = [m.upper() for m in item if m in ("get", "post", "put", "delete")]
        dep = any(item[m.lower()].get("deprecated") for m in methods)
        line = f"{','.join(methods):9} {path}{'   [DEPRECATED]' if dep else ''}"
        out.append(line)
        print("  " + line)
    gen = [l for l in out if "POST" in l and "text-to-cad" in l and "{id}" not in l]
    print("\n  REST generation endpoint present:", bool(gen), "->", gen or "NONE")
    with open(os.path.join(HERE, "data", "zoo-api-surface.txt"), "w") as fh:
        fh.write(f"Zoo API {spec['info'].get('version')} — derived from {ZOO_SPEC}\n\n")
        fh.write("\n".join(out) + "\n")
    print("  written to data/zoo-api-surface.txt")


if __name__ == "__main__":
    if "--zoo-spec" in sys.argv:
        zoo_spec()
    elif "--zoo-runs" in sys.argv:
        zoo_runs()
    else:
        baseline()
