"""Run 3 driver: a build goal over HTTP against the real model, followed as a client would.

Reads the owner's session from principals.json (never printed). Writes every change a
polling client could see to run3-progress.jsonl and a summary to run3-summary.json.
The watchdog kills the worker (by the pid in worker.pid) if tokens_spent passes HARD_STOP.
"""
import json, os, subprocess, sys, time, urllib.request, urllib.error

L = os.path.dirname(os.path.abspath(__file__))
API = "http://127.0.0.1:18480"
CEILING = int(os.environ.get("RUN3_CEILING", "70000"))
HARD_STOP = int(os.environ.get("RUN3_HARD_STOP", "85000"))
LIMIT_S = int(os.environ.get("RUN3_LIMIT_S", "1800"))
STATEMENT = ("a road-car wheel: a rim, a tyre, a hub, and five lug nuts on a polar pattern around the hub's axis. "
             "Build it in at most three steps.")

p = json.load(open(os.path.join(L, "principals.json"), encoding="utf-8"))
tok = p["owner"]["token"]


def http(method, path, body=None, timeout=300):
    data = None if body is None else json.dumps(body).encode()
    req = urllib.request.Request(API + path, data=data, method=method)
    if body is not None:
        req.add_header("Content-Type", "application/json")
    req.add_header("Authorization", "Bearer " + tok)
    t0 = time.time()
    try:
        with urllib.request.urlopen(req, timeout=timeout) as r:
            b, code = r.read(), r.status
    except urllib.error.HTTPError as e:
        b, code = e.read(), e.code
    try:
        return code, json.loads(b or b"null"), time.time() - t0
    except ValueError:
        return code, b[:500].decode("utf-8", "replace"), time.time() - t0


def say(*a):
    print(time.strftime("%H:%M:%S"), *a, flush=True)


out = open(os.path.join(L, "run3-progress.jsonl"), "w", encoding="utf-8")
summary = {"statement": STATEMENT, "ceiling": CEILING, "hard_stop": HARD_STOP}
code, r, took = http("POST", "/v1/goals", {"title": "Live verification: wheel build goal", "statement": STATEMENT,
                                          "project_id": p["project_id"], "build": True, "max_tokens": CEILING})
summary.update(create_status=code, create_s=round(took, 2))
if code != 201:
    say("create failed", code, json.dumps(r)[:800])
    json.dump(summary, open(os.path.join(L, "run3-summary.json"), "w"), indent=2)
    sys.exit(1)
gid = r["goal"]["id"]
summary.update(goal=gid, planned_tasks=[t["title"] for t in r["tasks"]], tokens_after_plan=r["goal"]["tokens_spent"],
               max_tokens=r["goal"].get("max_tokens"))
say("created", gid, "tasks", len(r["tasks"]), "tokens", r["goal"]["tokens_spent"], "in %.1fs" % took)
code, r2, took = http("POST", "/v1/goals/%s/start" % gid)
summary.update(start_status=code)
say("start", code)
if code != 200:
    say("start failed", json.dumps(r2)[:800])
    json.dump(summary, open(os.path.join(L, "run3-summary.json"), "w"), indent=2)
    sys.exit(1)

t0, last_sig, last_change = time.time(), None, time.time()
gaps, seen_gaps, watchdog = [], [], False
prev_seen = None
while time.time() - t0 < LIMIT_S:
    code, g, _ = http("GET", "/v1/goals/%s" % gid, timeout=60)
    if code != 200:
        say("read", code)
        time.sleep(1)
        continue
    goal, tasks = g["goal"], g["tasks"]
    seen = max((t.get("last_seen_at") or "" for t in tasks), default="") or None
    sig = (goal["status"], goal["tokens_spent"], tuple(t["status"] for t in tasks), seen)
    now = time.time()
    if sig != last_sig:
        gaps.append(round(now - last_change, 2))
        last_change = now
        rec = {"t": round(now - t0, 1), "status": goal["status"], "tokens": goal["tokens_spent"],
               "tasks": [t["status"] for t in tasks], "last_seen_at": seen}
        out.write(json.dumps(rec) + "\n"); out.flush()
        if sig[:3] != (last_sig or (None,) * 4)[:3]:
            say("t=%.0f" % rec["t"], goal["status"], "tokens", goal["tokens_spent"], rec["tasks"])
        last_sig = sig
    if goal["status"] not in ("active", "draft"):
        break
    if goal["tokens_spent"] >= HARD_STOP:
        say("WATCHDOG tokens", goal["tokens_spent"])
        pid = open(os.path.join(L, "worker.pid")).read().strip()
        subprocess.run(["taskkill", "/PID", pid, "/T", "/F"], capture_output=True)
        watchdog = True
        break
    time.sleep(1)

code, g, _ = http("GET", "/v1/goals/%s" % gid, timeout=60)
code, tl, _ = http("GET", "/v1/goals/%s/timeline" % gid, timeout=60)
events = tl.get("events", []) if isinstance(tl, dict) else []
summary.update(final_status=g["goal"]["status"], tokens_spent=g["goal"]["tokens_spent"],
               outcome=g["goal"].get("outcome_summary"), seconds=round(time.time() - t0, 1),
               tasks=[{k: t.get(k) for k in ("title", "status", "attempts", "error_code", "error_detail",
                                             "started_at", "ended_at")} for t in g["tasks"]],
               longest_unchanged_s=max(gaps) if gaps else None, gaps_over_10s=sum(1 for x in gaps if x > 10),
               changes=len(gaps), watchdog=watchdog,
               event_kinds=sorted({e["kind"] for e in events}),
               versions=[e for e in events if e["kind"] == "artifact.changed"])
json.dump(summary, open(os.path.join(L, "run3-summary.json"), "w", encoding="utf-8"), indent=2)
json.dump(g, open(os.path.join(L, "run3-goal.json"), "w", encoding="utf-8"), indent=2)
say("done", summary["final_status"], "tokens", summary["tokens_spent"], "longest unchanged %ss" % summary["longest_unchanged_s"])
