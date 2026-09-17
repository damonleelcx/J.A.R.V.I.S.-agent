"""Drive the real forged and forge-worker, in Linux containers, through the paths nobody had run.

Everything talks to the stand-in model (standin/main.go): zero live tokens. Postgres is the
local forge-pg (schema forge_unverified), blob storage the local forge-minio. The worker runs
in the pod's shape from deploy/k8s/31-worker.yaml (1 CPU, 2Gi, swap = memory, uid 10001),
forged in 30-forged.yaml's (1 CPU, 1Gi). amd64 under Docker Desktop's WSL2, NOT arm64.

    python uv.py up                 # network, schema, bucket, stand-in, forged, worker, sampler
    python uv.py principals         # owner / viewer / stranger + designs (host, principals.exe)
    python uv.py build  --tag long1 --steps 10 --stepdelay 12000
    python uv.py stop-held  --tag stop1
    python uv.py stop-kernel --tag stop2
    python uv.py approval --tag appr1 --decision approve
    python uv.py authz
    python uv.py exports
    python uv.py down

Each scenario appends one JSON object to data/results.jsonl and writes its own log lines.
"""

import argparse
import calendar
import json
import os
import secrets
import subprocess
import sys
import time
import urllib.error
import urllib.request

SCRATCH = os.environ.get("UV_SCRATCH", "C:/Users/damon/AppData/Local/Temp/claude/"
                         "C--Users-damon-Downloads-agents/9c73bcba-7500-4641-9433-f7c8efd05843/scratchpad/unverified")
BIN = SCRATCH + "/bin"
DATA = SCRATCH + "/data"
DOCS = SCRATCH + "/docs"
IMAGE = "forge-linux-test"
NET = "forge-uv-net"
STANDIN, FORGED, WORKER, SAMPLER = "forge-uv-standin", "forge-uv-forged", "forge-uv-worker", "forge-uv-sampler"
SCHEMA = "forge_unverified"
BUCKET = "forge-unverified-exports"
API = "http://127.0.0.1:18380"
DB = "postgres://forge:forge_dev_pw@host.docker.internal:55840/forge?sslmode=disable&search_path=" + SCHEMA

SAMPLER_SH = r'''
CID=$(cat /proc/1/cpuset 2>/dev/null)
while :; do
  t=$(date +%s.%N)
  for d in /proc/[0-9]*; do
    n=$(cat $d/comm 2>/dev/null) || continue
    case "$n" in
      forge-worker*|python*) awk -v t=$t -v p=${d#/proc/} -v n=$n '/^VmRSS:|^VmHWM:/{a[$1]=$2} END{print "P",t,p,n,a["VmRSS:"]+0,a["VmHWM:"]+0}' $d/status 2>/dev/null;;
    esac
  done
  c=/sys/fs/cgroup/docker/$WID
  echo "C $t $(cat $c/memory.current 2>/dev/null) $(awk '$1=="anon"||$1=="file"{printf "%s ", $2}' $c/memory.stat 2>/dev/null)"
  sleep 0.25
done
'''


def now():
    return time.time()


def stamp(t=None):
    t = now() if t is None else t
    return time.strftime("%H:%M:%S", time.localtime(t)) + ".%03d" % int((t % 1) * 1000)


def say(*a):
    line = stamp() + " " + " ".join(str(x) for x in a)
    print(line, flush=True)
    with open(DATA + "/run.log", "a", encoding="utf-8") as f:
        f.write(line + "\n")


def sh(*cmd, check=True, timeout=300):
    r = subprocess.run(list(cmd), capture_output=True, text=True, encoding="utf-8", errors="replace",
                       timeout=timeout)
    if check and r.returncode != 0:
        raise SystemExit("%s: %s" % (" ".join(cmd), (r.stdout + r.stderr)[-2000:]))
    return r


def psql(q):
    out = sh("docker", "exec", "forge-pg", "psql", "-U", "forge", "-d", "forge", "-AtX", "-c",
             "set search_path=%s; %s" % (SCHEMA, q)).stdout
    return [l for l in out.splitlines() if l != "SET"]


def result(obj):
    obj["recorded_at"] = stamp()
    with open(DATA + "/results.jsonl", "a", encoding="utf-8") as f:
        f.write(json.dumps(obj) + "\n")
    say("RESULT", json.dumps(obj)[:1500])


# ---- HTTP ------------------------------------------------------------------

def principals():
    with open(SCRATCH + "/principals.json", encoding="utf-8") as f:
        return json.load(f)


def http(method, path, who=None, body=None, raw=False, timeout=180):
    """(status, parsed body or bytes, seconds). who is a principal dict or None."""
    data = None if body is None else json.dumps(body).encode()
    req = urllib.request.Request(API + path, data=data, method=method)
    if body is not None:
        req.add_header("Content-Type", "application/json")
    if who is not None:
        req.add_header("Authorization", "Bearer " + who["token"])
    t0 = now()
    try:
        with urllib.request.urlopen(req, timeout=timeout) as r:
            b = r.read()
            code = r.status
    except urllib.error.HTTPError as e:
        b = e.read()
        code = e.code
    took = now() - t0
    if raw:
        return code, b, took
    try:
        return code, json.loads(b or b"null"), took
    except ValueError:
        return code, b[:300].decode("utf-8", "replace"), took


# ---- containers ------------------------------------------------------------

def common_env():
    return {
        "FORGE_ENV": "development", "FORGE_DATABASE_URL": DB, "FORGE_LOG_FORMAT": "json", "FORGE_LOG_LEVEL": "info",
        "FORGE_LLM_BASE_URL": "http://%s:18390/v1" % STANDIN, "FORGE_LLM_API_KEY": "stand-in-not-a-key",
        "FORGE_LLM_CONVERSE_MODEL": "si-converse", "FORGE_LLM_VISION_MODEL": "si-vision",
        "FORGE_LLM_PLANNER_MODEL": "si-planner", "FORGE_LLM_EXECUTOR_MODEL": "si-executor",
        "FORGE_LLM_VERIFIER_MODEL": "other-verifier",
        "FORGE_DATA_BOUNDARY": "no_training", "FORGE_MAX_TOKENS_PER_GOAL": "300000",
        "FORGE_CAD_PYTHON": "/usr/local/bin/python", "FORGE_CAD_POOL": "1", "FORGE_ALLOW_SCRIPTS": "false",
        "FORGE_BLOB_BUCKET": BUCKET, "FORGE_BLOB_REGION": "us-east-1",
        "FORGE_BLOB_ENDPOINT": "http://host.docker.internal:55841",
        "AWS_ACCESS_KEY_ID": "forge", "AWS_SECRET_ACCESS_KEY": "forge_dev_minio",
        "HOME": "/tmp",
    }


def env_args(e):
    out = []
    for k, v in e.items():
        out += ["-e", "%s=%s" % (k, v)]
    return out


def start_worker():
    sh("docker", "rm", "-f", WORKER, check=False)
    e = common_env()
    e.update({"FORGE_WORKER_CONCURRENCY": "1", "FORGE_WORKSPACE_ROOT": "/tmp/ws"})
    sh("docker", "run", "-d", "--name", WORKER, "--network", NET, "--add-host", "host.docker.internal:host-gateway",
       "--cpus", "1", "--memory", "2g", "--memory-swap", "2g", "--user", "10001",
       "-v", "%s:/opt/forge:ro" % BIN, *env_args(e), "--entrypoint", "/opt/forge/forge-worker", IMAGE)
    wid = sh("docker", "inspect", "-f", "{{.Id}}", WORKER).stdout.strip()
    sh("docker", "rm", "-f", SAMPLER, check=False)
    sh("docker", "run", "-d", "--name", SAMPLER, "--pid", "container:" + WORKER, "--cgroupns", "host",
       "-e", "WID=" + wid, "-v", "%s:/data" % DATA, "--entrypoint", "sh", IMAGE, "-c",
       SAMPLER_SH.replace("\n  sleep 0.25", "\n  sleep 0.25") + "", )
    # the sampler's stdout is the record; follow it into a file
    subprocess.Popen(["docker", "logs", "-f", SAMPLER], stdout=open(DATA + "/sampler-%d.txt" % int(now()), "w"),
                     stderr=subprocess.STDOUT)
    for _ in range(60):
        logs = sh("docker", "logs", WORKER, check=False).stdout + sh("docker", "logs", WORKER, check=False).stderr
        if "forge.worker.ready" in logs:
            say("worker ready", wid[:12])
            return wid
        time.sleep(0.5)
    raise SystemExit("worker did not become ready:\n" + logs[-3000:])


def up(_args):
    os.makedirs(DATA, exist_ok=True)
    sh("docker", "network", "create", NET, check=False)
    for q in ["drop schema if exists %s cascade" % SCHEMA, "create schema %s" % SCHEMA]:
        sh("docker", "exec", "forge-pg", "psql", "-U", "forge", "-d", "forge", "-c", q)
    sh("docker", "exec", "forge-minio", "sh", "-c",
       "mc alias set uv http://127.0.0.1:9000 forge forge_dev_minio >/dev/null && mc mb -p uv/" + BUCKET)
    sh("docker", "rm", "-f", STANDIN, FORGED, check=False)
    sh("docker", "run", "--rm", "--network", NET, "--add-host", "host.docker.internal:host-gateway",
       "-v", "%s:/opt/forge:ro" % BIN, *env_args(common_env()), "--entrypoint", "/opt/forge/forgectl", IMAGE, "migrate")
    sh("docker", "run", "-d", "--name", STANDIN, "--network", NET, "-v", "%s:/opt/forge:ro" % BIN,
       "-v", "%s:/data" % DATA, "--entrypoint", "/opt/forge/standin", IMAGE,
       "-addr", "0.0.0.0:18390", "-log", "/data/standin.jsonl")
    e = common_env()
    e.update({"FORGE_HTTP_ADDR": ":8080", "FORGE_PUBLIC_URL": "http://127.0.0.1:18380",
              "FORGE_SESSION_SECRET": secrets.token_urlsafe(48), "FORGE_MAIL_OUTBOX_DIR": "/tmp/outbox"})
    sh("docker", "run", "-d", "--name", FORGED, "--network", NET, "--add-host", "host.docker.internal:host-gateway",
       "--cpus", "1", "--memory", "1g", "--memory-swap", "1g", "--user", "10001", "-p", "127.0.0.1:18380:8080",
       "-v", "%s:/opt/forge:ro" % BIN, *env_args(e), "--entrypoint", "/opt/forge/forged", IMAGE)
    for _ in range(60):
        try:
            code, _, _ = http("GET", "/readyz")
            if code == 200:
                break
        except Exception:
            pass
        time.sleep(0.5)
    else:
        raise SystemExit("forged not ready:\n" + sh("docker", "logs", FORGED, check=False).stderr[-3000:])
    say("forged ready")
    start_worker()


def down(_args):
    sh("docker", "rm", "-f", SAMPLER, WORKER, FORGED, STANDIN, check=False)
    say("containers removed")


# ---- observation helpers ---------------------------------------------------

def standin_calls(tag=None, t_from=None, t_to=None):
    """The stand-in's calls, by tag or by the window they were made in.

    A look carries no statement, so it carries no tag: token reconciliation is by window.
    Scenarios run one after another against one worker, so a window is one goal's calls.
    """
    out = []
    try:
        with open(DATA + "/standin.jsonl", encoding="utf-8") as f:
            for line in f:
                o = json.loads(line)
                at = calendar.timegm(time.strptime(o["at"][:19], "%Y-%m-%dT%H:%M:%S")) + int(o["at"][20:23]) / 1000
                o["t"] = at
                if tag is not None and o.get("tag") != tag:
                    continue
                if t_from is not None and not (t_from <= at <= t_to):
                    continue
                out.append(o)
    except FileNotFoundError:
        pass
    return out


def answered_tokens(t_from, t_to):
    calls = standin_calls(None, t_from, t_to)
    kinds = {}
    for c in calls:
        k = "%s/%s" % (c["kind"], c.get("outcome"))
        kinds[k] = kinds.get(k, 0) + 1
    return sum(c["tokens"] for c in calls if c.get("outcome") == "answered"), kinds


def goal_state(gid, who):
    code, g, _ = http("GET", "/v1/goals/" + gid, who)
    code2, tl, _ = http("GET", "/v1/goals/%s/timeline" % gid, who)
    if code != 200 or code2 != 200:
        raise SystemExit("goal read %d/%d: %s %s" % (code, code2, g, tl))
    return g, tl["events"]


def create_build(p, tag, statement_extra, title):
    t_window = now()
    body = {"title": title, "statement": "Build rows of boxes as a model. tag=%s %s" % (tag, statement_extra),
            "project_id": p["project_id"], "build": True}
    code, r, took = http("POST", "/v1/goals", p["owner"], body)
    if code != 201:
        raise SystemExit("create %d: %s" % (code, r))
    gid = r["goal"]["id"]
    say("created build goal", gid, "in %.2fs" % took, "tasks", len(r["tasks"]))
    code, r2, took2 = http("POST", "/v1/goals/%s/start" % gid, p["owner"])
    if code != 200:
        raise SystemExit("start %d: %s" % (code, r2))
    say("started", gid, "in %.2fs" % took2)
    return gid, {"t_window": t_window, "create_status": 201, "create_s": round(took, 3), "tasks": len(r["tasks"]),
                 "start_status": code, "start_s": round(took2, 3)}


def follow(gid, who, limit_s, every=1.0, stop_when=None):
    """Poll the goal and its timeline as a client would; record every change a client could see."""
    changes, last_sig, t0 = [], None, now()
    polls = []
    while now() - t0 < limit_s:
        tp = now()
        g, ev = goal_state(gid, who)
        polls.append(round(now() - tp, 3))
        tasks = g["tasks"]
        # last_seen_at is the running task's "still at work" stamp (NFR-02), present only
        # on binaries that have it; absent, it is None and changes nothing.
        sig = (g["goal"]["status"], g["goal"]["tokens_spent"], len(ev),
               tuple(t["status"] for t in tasks), tuple(t.get("last_seen_at") for t in tasks))
        if sig != last_sig:
            changes.append({"t": round(now() - t0, 2), "status": sig[0], "tokens": sig[1], "events": sig[2],
                            "done": sum(1 for s in sig[3] if s == "succeeded"), "of": len(sig[3]),
                            "last_event": max(ev, key=lambda e: e["seq"])["kind"] if ev else None,
                            "last_seen_at": max((x for x in sig[4] if x), default=None)})
            last_sig = sig
        if g["goal"]["status"] not in ("active", "draft"):
            return g, ev, changes, polls
        if stop_when and stop_when(g, ev):
            return g, ev, changes, polls
        time.sleep(every)
    return g, ev, changes, polls


# ---- scenarios -------------------------------------------------------------

def build(args):
    p = principals()
    extra = "steps=%d parts=%d stepdelay=%d" % (args.steps, args.parts, args.stepdelay)
    t_window = now()
    gid, meta = create_build(p, args.tag, extra, "Long build (stand-in) " + args.tag)
    t0 = now()
    g, ev, changes, polls = follow(gid, p["owner"], args.limit)
    took = now() - t0
    gaps = [round(b["t"] - a["t"], 2) for a, b in zip(changes, changes[1:])]
    time.sleep(1)
    answered, kinds = answered_tokens(t_window, now())
    versions = [e for e in ev if e["kind"] == "artifact.changed"]
    result({"scenario": "build", "tag": args.tag, "goal": gid, **meta, "knobs": extra,
            "final": g["goal"]["status"], "wall_s": round(took, 1), "tokens_spent": g["goal"]["tokens_spent"],
            "standin_answered_tokens": answered, "calls": kinds,
            "tasks": [(t["title"], t["status"], t["attempts"]) for t in g["tasks"]],
            "versions_kept": len(versions), "events": len(ev),
            "changes": changes, "max_gap_s": max(gaps) if gaps else None, "gaps_s": gaps,
            "poll_p50_s": sorted(polls)[len(polls) // 2] if polls else None, "polls": len(polls)})


def task_row(gid, n):
    rows = psql("select idempotency_key||'|'||status||'|'||attempt_count||'|'||coalesce(lease_owner,'-')"
                " from forge_tasks where goal_id='%s' order by created_at, idempotency_key offset %d limit 1" % (gid, n - 1))
    return rows[0].split("|") if rows else None


def worker_exit_wait(limit=60):
    for _ in range(int(limit / 0.1)):
        st = sh("docker", "inspect", "-f", "{{.State.Status}} {{.State.ExitCode}} {{.State.FinishedAt}}", WORKER).stdout.split()
        if st[0] == "exited":
            return st
        time.sleep(0.1)
    return st


def stop_common(args, gid, meta, step, trigger_desc, trigger):
    p = principals()
    # wait for the trigger
    t0 = now()
    while now() - t0 < 300:
        if trigger():
            break
        time.sleep(0.1)
    else:
        raise SystemExit("trigger never fired: " + trigger_desc)
    running = psql("select n from (select status, row_number() over (order by created_at, idempotency_key) n"
                   " from forge_tasks where goal_id='%s') t where status in ('running','claimed','verifying')" % gid)
    if running:
        step = int(running[0])
    before = task_row(gid, step)
    spent_before = int(psql("select tokens_spent from forge_goals where id='%s'" % gid)[0])
    t_stop = now()
    sh("docker", "kill", "--signal=TERM", WORKER)
    say("SIGTERM sent to", WORKER, "task before:", before, "spent", spent_before)
    handed, samples = None, 0
    while now() - t_stop < 90:
        row = task_row(gid, step)
        samples += 1
        if row[1] == "ready" and row[3] == "-":
            handed = round(now() - t_stop, 3)
            break
        time.sleep(0.05)
    ex = worker_exit_wait()
    exit_s = None
    logs = sh("docker", "logs", WORKER, check=False)
    wlog = logs.stdout + logs.stderr
    with open(DATA + "/worker-%s-stopped.log" % args.tag, "w", encoding="utf-8") as f:
        f.write(wlog)
    after = task_row(gid, step)
    events = psql("select kind||'|'||coalesce(task_id,'')||'|'||summary from forge_events where goal_id='%s' order by seq" % gid)
    warn_lines = [l for l in wlog.splitlines() if '"level":"WARN"' in l or '"level":"ERROR"' in l]
    say("stopped: handed back after", handed, "s; exit", ex, "; after", after)
    # restart
    time.sleep(args.restart_after)
    t_restart = now()
    start_worker()
    g, ev, changes, _ = follow(gid, p["owner"], args.limit)
    time.sleep(1)
    per_step = {}
    for c in standin_calls(args.tag):
        if c["kind"] == "step":
            per_step.setdefault(c["step"], []).append(c["outcome"])
    answered, kinds = answered_tokens(meta["t_window"], now())
    result({"scenario": "stop-" + trigger_desc, "tag": args.tag, "goal": gid, **meta, "stopped_step": step,
            "task_before": before, "task_after": after, "spent_before_stop": spent_before,
            "handed_back_s": handed, "row_samples": samples, "worker_exit": ex,
            "handed_back_events": [e for e in events if e.startswith("task.handed_back")],
            "worker_warn_or_error_lines": warn_lines[-10:],
            "final": g["goal"]["status"], "tokens_spent": g["goal"]["tokens_spent"],
            "standin_answered_tokens": answered, "calls": kinds, "step_requests": per_step,
            "tasks": [(t["title"], t["status"], t["attempts"]) for t in g["tasks"]],
            "resume_s": round(now() - t_restart, 1),
            "events_kinds": [e["kind"] for e in ev]})


def stop_held(args):
    p = principals()
    step = 3
    gid, meta = create_build(p, args.tag, "steps=4 parts=256 hold=%d:120000" % step, "Stop mid model call " + args.tag)

    def trig():
        # The held request is in flight: step 3 has been running for 5 s, and the stand-in
        # holds its first reply for 120 s, so the worker is inside the model call.
        age = psql("select coalesce(extract(epoch from now() - started_at), 0)::int from forge_tasks"
                   " where goal_id='%s' and status='running' order by created_at, idempotency_key offset 0 limit 1" % gid)
        row = task_row(gid, step)
        return bool(row and row[1] == "running" and age and int(age[0]) >= 5)
    stop_common(args, gid, meta, step, "held-model-call", trig)


def stop_kernel(args):
    p = principals()
    step = 2
    gid, meta = create_build(p, args.tag, "steps=3 parts=4096 lookdelay=30000", "Stop after the step was paid for " + args.tag)

    def trig():
        # Step 2's reply was delivered and charged; its look is held 30 s, so the worker
        # is between paying for the step and keeping it.
        return any(c["kind"] == "step" and c.get("step") == 2 and c.get("outcome") == "answered"
                   for c in standin_calls(args.tag))
    stop_common(args, gid, meta, step, "after-step-paid", trig)


def approval(args):
    p = principals()
    body = {"title": "Approval path (stand-in) " + args.tag, "statement": "approvalplan tag=%s" % args.tag,
            "risk_tier": "r2", "project_id": p["project_id"]}
    t_window = now()
    code, r, took = http("POST", "/v1/goals", p["owner"], body)
    if code != 201:
        raise SystemExit("create %d: %s" % (code, r))
    gid = r["goal"]["id"]
    tasks = [(t["title"], t["risk_tier"], t["requires_approval"]) for t in r["tasks"]]
    code, _, _ = http("POST", "/v1/goals/%s/start" % gid, p["owner"])
    say("approval goal", gid, tasks, "start", code)
    t0 = now()
    appr = None
    while now() - t0 < 120:
        c, a, _ = http("GET", "/v1/approvals", p["owner"])
        mine = [x for x in a["approvals"] if x["goal_id"] == gid]
        if mine:
            appr = mine[0]
            break
        time.sleep(0.5)
    if not appr:
        raise SystemExit("no approval requested within 120 s")
    t_req = now() - t0
    g, ev = goal_state(gid, p["owner"])
    gated = [(t["title"], t["status"]) for t in g["tasks"]]
    _, va, _ = http("GET", "/v1/approvals", p["viewer"])
    _, sa, _ = http("GET", "/v1/approvals", p["stranger"])
    checks = {
        "viewer_lists_it": any(x["id"] == appr["id"] for x in va["approvals"]),
        "stranger_lists_it": any(x["id"] == appr["id"] for x in sa["approvals"]),
        "viewer_decide": http("POST", "/v1/approvals/" + appr["id"], p["viewer"], {"decision": "approve", "reason": "viewer"})[0],
        "stranger_decide": http("POST", "/v1/approvals/" + appr["id"], p["stranger"], {"decision": "approve", "reason": "stranger"})[0],
        "anonymous_decide": http("POST", "/v1/approvals/" + appr["id"], None, {"decision": "approve"})[0],
    }
    still = psql("select decision from forge_approvals where id='%s'" % appr["id"])[0]
    code, d, _ = http("POST", "/v1/approvals/" + appr["id"], p["owner"],
                      {"decision": args.decision, "reason": "owner decided in the unverified-paths run"})
    t_dec = now()
    second = http("POST", "/v1/approvals/" + appr["id"], p["owner"], {"decision": args.decision, "reason": "twice"})[0]
    g, ev, changes, _ = follow(gid, p["owner"], args.limit)
    result({"scenario": "approval-" + args.decision, "tag": args.tag, "goal": gid, "planned": tasks,
            "approval_requested_after_s": round(t_req, 2), "tasks_when_gated": gated,
            "approval_requested_events": sum(1 for e in ev if e["kind"] == "approval.requested"),
            "refusals": checks, "decision_after_refusals": still, "owner_decide": code, "owner_decide_body": d,
            "decided_twice": second, "final": g["goal"]["status"], "settled_after_decision_s": round(now() - t_dec, 1),
            "tasks": [(t["title"], t["status"], t["attempts"], t.get("error_detail", "")) for t in g["tasks"]],
            "events": [(e["kind"], e["actor"], e["summary"][:100]) for e in ev],
            "tokens_spent": g["goal"]["tokens_spent"],
            "standin_answered_tokens": answered_tokens(t_window, now())[0], "calls": answered_tokens(t_window, now())[1]})


def authz(_args):
    """Every goal, approval and export route, as anonymous, stranger, viewer and owner, over HTTP."""
    p = principals()
    q = lambda s: psql(s)[0] if psql(s) else ""
    goal = q("select g.id from forge_goals g join forge_tasks t on t.goal_id=g.id where g.project_id='%s' and g.status='succeeded' limit 1" % p["project_id"])
    appr = q("select a.id from forge_approvals a join forge_goals g on g.id=a.goal_id where g.project_id='%s' limit 1" % p["project_id"])
    ver = list(p["designs"].values())[0]
    exp = q("select e.id from forge_geometry_exports e where e.project_id='%s' limit 1" % p["project_id"]) \
        if psql("select to_regclass('forge_geometry_exports')")[0] else ""
    routes = [
        ("GET", "/v1/goals", None), ("POST", "/v1/goals", {"title": "authz", "statement": "authz probe tag=authz", "project_id": p["project_id"]}),
        ("POST", "/v1/goals/%s/plan" % goal, {}), ("POST", "/v1/goals/%s/start" % goal, None),
        ("GET", "/v1/goals/%s" % goal, None), ("GET", "/v1/goals/%s/timeline" % goal, None),
        ("GET", "/v1/approvals", None), ("POST", "/v1/approvals/%s" % appr, {"decision": "approve", "reason": "probe"}),
        ("GET", "/v1/geometry/%s/export?format=step" % ver, None), ("GET", "/v1/geometry/%s/export/label?format=step" % ver, None),
        ("POST", "/v1/geometry/%s/exports" % ver, None), ("GET", "/v1/geometry/exports/%s" % exp, None),
        ("GET", "/v1/geometry/exports/%s/file" % exp, None),
    ]
    rows = []
    for method, path, body in routes:
        row = {"route": method + " " + path}
        for who in ("anonymous", "stranger", "viewer"):
            code, b, _ = http(method, path, None if who == "anonymous" else p[who], body, raw=True)
            row[who] = code
        rows.append(row)
        say(row)
    result({"scenario": "authz-http", "goal": goal, "approval": appr, "version": ver, "export": exp, "rows": rows})


def mem_window(t_from, t_to):
    """Peaks from the sampler files over a window: kernel (python) RSS, worker RSS, cgroup current."""
    kern, work, cg = [], [], []
    for name in os.listdir(DATA):
        if not name.startswith("sampler-"):
            continue
        with open(DATA + "/" + name, encoding="utf-8", errors="replace") as f:
            for line in f:
                s = line.split()
                if len(s) < 3:
                    continue
                try:
                    t = float(s[1])
                except ValueError:
                    continue
                if not (t_from <= t <= t_to):
                    continue
                if s[0] == "P" and len(s) >= 6:
                    (kern if s[3].startswith("python") else work).append((t, int(s[4]) // 1024, int(s[5]) // 1024))
                elif s[0] == "C" and len(s) >= 3 and s[2].isdigit():
                    cg.append((t, int(s[2]) >> 20))
    pk = lambda xs, i: max((x[i] for x in xs), default=None)
    last = lambda xs, i: (sorted(xs)[-1][i] if xs else None)
    first = lambda xs, i: (sorted(xs)[0][i] if xs else None)
    return {"kernel_rss_first_mib": first(kern, 1), "kernel_rss_peak_mib": pk(kern, 1), "kernel_rss_last_mib": last(kern, 1),
            "kernel_hwm_mib": pk(kern, 2), "worker_rss_peak_mib": pk(work, 1), "worker_rss_last_mib": last(work, 1),
            "cgroup_first_mib": first(cg, 1), "cgroup_peak_mib": pk(cg, 1), "cgroup_last_mib": last(cg, 1), "samples": len(cg)}


def cgroup_counters():
    r = sh("docker", "exec", WORKER, "sh", "-c",
           "cat /sys/fs/cgroup/memory.peak; echo ---; cat /sys/fs/cgroup/memory.events; echo ---; cat /sys/fs/cgroup/cpu.stat",
           check=False).stdout
    parts = r.split("---")
    try:
        return {"memory_peak_mib": int(parts[0].strip()) >> 20,
                "oom_kill": dict(l.split() for l in parts[1].strip().splitlines()).get("oom_kill"),
                "throttled_s": int(dict(l.split() for l in parts[2].strip().splitlines()).get("throttled_usec", 0)) / 1e6}
    except Exception as e:
        return {"cgroup_error": "%s: %r" % (e, r[:300])}


def exports(args):
    p = principals()
    for name in args.designs.split(","):
        ver = p["designs"][name]
        settle_before = now()
        time.sleep(args.settle)
        before = mem_window(now() - args.settle, now())
        t0 = now()
        code, r, took = http("POST", "/v1/geometry/%s/exports" % ver, p["viewer"])
        if code != 202:
            result({"scenario": "export", "design": name, "request_status": code, "body": r})
            continue
        e = r["export"]
        seen, running_at = [], None
        while now() - t0 < args.limit:
            c, s, _ = http("GET", "/v1/geometry/exports/" + e["id"], p["viewer"])
            st = s["export"]["status"]
            if not seen or seen[-1][1] != st:
                seen.append((round(now() - t0, 2), st))
            if st in ("succeeded", "failed"):
                break
            time.sleep(0.5)
        t_done = now()
        fin = s["export"]
        dl_code, blob, dl_s = http("GET", "/v1/geometry/exports/%s/file" % e["id"], p["viewer"], raw=True, timeout=600)
        time.sleep(args.settle)
        during = mem_window(t0, t_done)
        after = mem_window(t_done + args.settle - 2, now())
        result({"scenario": "export", "design": name, "version": ver, "export": e["id"], "request_s": round(took, 3),
                "statuses": seen, "final": fin["status"], "reason": fin.get("reason"), "parts": fin.get("parts"),
                "size_bytes": fin.get("size_bytes"), "wall_s": round(t_done - t0, 1),
                "download_status": dl_code, "download_bytes": len(blob), "download_s": round(dl_s, 2),
                "mem_before": before, "mem_during": during, "mem_after": after, "cgroup": cgroup_counters()})


def main():
    ap = argparse.ArgumentParser()
    sub = ap.add_subparsers(dest="cmd", required=True)
    sub.add_parser("up")
    sub.add_parser("down")
    sub.add_parser("worker")
    b = sub.add_parser("build")
    b.add_argument("--tag", required=True)
    b.add_argument("--steps", type=int, default=10)
    b.add_argument("--parts", type=int, default=640)
    b.add_argument("--stepdelay", type=int, default=12000)
    b.add_argument("--limit", type=float, default=1200)
    for name in ("stop-held", "stop-kernel"):
        s = sub.add_parser(name)
        s.add_argument("--tag", required=True)
        s.add_argument("--restart-after", type=float, default=3)
        s.add_argument("--limit", type=float, default=600)
    a = sub.add_parser("approval")
    a.add_argument("--tag", required=True)
    a.add_argument("--decision", choices=["approve", "reject"], required=True)
    a.add_argument("--limit", type=float, default=180)
    sub.add_parser("authz")
    x = sub.add_parser("exports")
    x.add_argument("--designs", default="barrel-8192.json,barrel-30400.json,barrel-89744.json")
    x.add_argument("--settle", type=float, default=8)
    x.add_argument("--limit", type=float, default=900)
    args = ap.parse_args()
    os.makedirs(DATA, exist_ok=True)
    {"up": up, "down": down, "worker": lambda a: start_worker(), "build": build, "stop-held": stop_held,
     "stop-kernel": stop_kernel, "approval": approval, "authz": authz, "exports": exports}[args.cmd](args)


if __name__ == "__main__":
    main()
