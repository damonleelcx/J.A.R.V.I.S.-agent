"""Premise B: can the model produce a CONSTRAINED PARAMETRIC document?

Premise A proved the kernel builds and exports. It also proved the harder half:
named parameters are not enough — `rib_length` as an independent 52 mm broke the
model at plate_size=50, and the same model survived a 3.4x size sweep once
rib_length was DERIVED from plate_size.

So this asks the model for exactly that shape of output and measures three
things over N runs, the way internal/eval does: a rate, not a verdict, with
every reply kept.
"""
import json, os, sys, time, urllib.request

BASE = os.environ["FORGE_LLM_BASE_URL"].rstrip("/")
KEY = os.environ["FORGE_LLM_API_KEY"]
MODEL = os.environ.get("FORGE_LLM_CONVERSE_MODEL", "qwen-plus")
RUNS = int(sys.argv[1]) if len(sys.argv) > 1 else 3

PROMPT = """Design a bracket that mounts a NEMA 17 stepper motor to a flat surface,
with two stiffening ribs.

Reply with JSON only, in this shape:

{
  "parameters": [
    {"name": "snake_case_name", "value": 60.0, "unit": "mm",
     "how": "chosen" | "standard",
     "source": "where a standard figure came from, else \\"\\""}
  ],
  "derived": [
    {"name": "snake_case_name", "expression": "plate_size - 2 * edge_margin",
     "why": "what relationship this keeps true when other parameters change"}
  ],
  "features": [
    {"id": "stable-kebab-id", "op": "extrude" | "hole" | "fillet",
     "of": "what it applies to", "uses": ["parameter names this feature reads"]}
  ]
}

Rules:
- Every number a person could change is a PARAMETER with a unit.
- Anything whose correct value DEPENDS on another parameter goes in "derived"
  as an expression, never as a fixed number. A rib whose length does not follow
  the plate it sits on will overhang the plate when the plate shrinks.
- "standard" means you are quoting a published figure; say which in "source".
  "chosen" means you picked it."""

def call():
    req = urllib.request.Request(
        BASE + "/chat/completions",
        data=json.dumps({
            "model": MODEL,
            "messages": [{"role": "user", "content": PROMPT}],
            "response_format": {"type": "json_object"},
        }).encode(),
        headers={"Authorization": "Bearer " + KEY, "Content-Type": "application/json"},
    )
    t0 = time.perf_counter()
    with urllib.request.urlopen(req, timeout=180) as r:
        body = json.load(r)
    return body["choices"][0]["message"]["content"], round(time.perf_counter() - t0, 1)

results = []
for i in range(RUNS):
    try:
        raw, secs = call()
        doc = json.loads(raw)
    except Exception as e:
        results.append({"run": i + 1, "error": str(e)[:200]})
        continue
    params = doc.get("parameters") or []
    derived = doc.get("derived") or []
    feats = doc.get("features") or []
    results.append({
        "run": i + 1, "seconds": secs,
        "n_parameters": len(params),
        "n_derived": len(derived),
        "n_features": len(feats),
        "all_have_units": bool(params) and all(p.get("unit") for p in params),
        "standards_are_sourced": all(
            p.get("source") for p in params if p.get("how") == "standard"),
        "nema_31mm_present": any(
            abs(float(p.get("value", 0)) - 31.0) < 0.01 for p in params),
        "derived_names": [d.get("name") for d in derived],
        "raw": doc,
    })

held = [r for r in results if "error" not in r]
print(json.dumps({
    "model": MODEL, "runs": RUNS, "completed": len(held),
    "rate_any_derived": f"{sum(1 for r in held if r['n_derived'] > 0)}/{len(held)}",
    "rate_units_on_all": f"{sum(1 for r in held if r['all_have_units'])}/{len(held)}",
    "rate_standards_sourced": f"{sum(1 for r in held if r['standards_are_sourced'])}/{len(held)}",
    "rate_nema_31mm": f"{sum(1 for r in held if r['nema_31mm_present'])}/{len(held)}",
    "per_run": [{k: v for k, v in r.items() if k != "raw"} for r in results],
}, indent=2))
with open(sys.argv[2] if len(sys.argv) > 2 else "premise_b.json", "w") as f:
    json.dump(results, f, indent=2)
