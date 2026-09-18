"""Print the interleaved before/after rows from data/results-*.jsonl as a markdown table."""
import json
import os
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
rows = []
for name in sys.argv[1:] or ("results-90k-300k.jsonl", "results-1m.jsonl"):
    with open(os.path.join(HERE, "data", name)) as fh:
        rows += [json.loads(line) for line in fh if line.strip()]
print("| occurrences | mode | sidecar | build s | shapes | properties | answer sha256 | properties sha256 | peak GB | host CPU |")
print("|---:|---|---|---:|---:|---:|---|---|---:|---:|")
for r in rows:
    ph = r.get("phases") or {}
    side = "main" if "main" in os.path.basename(r["sidecar"]) else "this branch"
    print("| {:,} | {} | {} | {:.1f} | {:.1f} | {} | `{}` | `{}` | {:.2f} | {:.0f}% |".format(
        r["parts"], r["mode"], side, r["build_s"], ph.get("shapes", 0),
        "%.1f" % ph["properties"] if "properties" in ph else "-",
        (r.get("answer_sha256") or "")[:12], (r.get("properties_sha256") or "-")[:12],
        r["peak_rss_gb"], r.get("host_cpu_mean_pct") or 0))
