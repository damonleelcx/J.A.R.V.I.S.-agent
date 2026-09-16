# Per-step tokens and calls for the live build goal, from forge-worker's debug log
# (forge.llm.completed carries prompt/completion tokens) attributed by time to the
# task that was running, plus the database's own record of each step.
import glob, json, os, re, subprocess, sys
from datetime import datetime

L = os.path.dirname(os.path.abspath(__file__))
goal = open(os.path.join(L, "goal.id")).read().strip()

def psql(q):
    out = subprocess.run(["docker", "exec", "forge-pg", "psql", "-U", "forge", "-d", "forge", "-Atc", q],
                         capture_output=True, text=True, encoding="utf-8").stdout
    return [l.split("|") for l in out.strip().splitlines() if l]

def ts(s):
    return datetime.fromisoformat(s.replace("Z", "+00:00"))

calls = []
for f in sorted(glob.glob(os.path.join(L, "worker-*.log"))):
    for line in open(f, encoding="utf-8", errors="replace"):
        if "msg=forge.llm.completed" not in line:
            continue
        m = dict(re.findall(r'(\w+)=("[^"]*"|\S+)', line))
        calls.append((ts(m["time"]), os.path.basename(f), m.get("role"), m.get("model"),
                      int(m.get("prompt_tokens", 0)), int(m.get("completion_tokens", 0)), int(m.get("latency_ms", 0))))

# Attempts: tool-call ledger rows give each attempt's start and end.
rows = psql(f"""select t.idempotency_key, c.attempt, c.started_at, c.ended_at
  from forge_tool_calls c join forge_tasks t on t.id = c.task_id where t.goal_id = '{goal}' order by c.started_at""")
tasks = psql(f"""select idempotency_key, status, attempt_count, started_at, ended_at,
  coalesce(result->'result'->>'version_id',''), coalesce(result->'result'->>'parts',''),
  coalesce(result->>'summary', error_detail, '') from forge_tasks where goal_id = '{goal}' order by idempotency_key""")

spans = []
for key, status, attempts, started, ended, ver, parts, summary in tasks:
    if started:
        spans.append((key, ts(started), ts(ended) if ended else None))

def owner(t):
    best = None
    for key, s, e in spans:
        if s <= t and (e is None or t <= e):
            best = key
    return best or "planning/unattributed"

per = {}
for t, f, role, model, p, c, lat in calls:
    k = owner(t)
    d = per.setdefault(k, {"calls": 0, "prompt": 0, "completion": 0, "roles": {}})
    d["calls"] += 1; d["prompt"] += p; d["completion"] += c
    d["roles"][role] = d["roles"].get(role, 0) + 1

print(json.dumps({"goal": goal, "calls_logged": len(calls),
                  "tokens_logged": sum(p + c for _, _, _, _, p, c, _ in calls),
                  "per_step": per,
                  "tasks": [dict(zip(["key", "status", "attempts", "started", "ended", "version", "parts", "summary"], t)) for t in tasks],
                  "attempts": rows}, indent=1, default=str))
