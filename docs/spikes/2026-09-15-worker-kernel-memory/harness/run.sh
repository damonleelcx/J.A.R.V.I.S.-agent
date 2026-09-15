#!/usr/bin/env bash
# run.sh <label> <pool> <concurrency> <occurrences> <copies>
# Creates <copies> build goals of <occurrences> in the forge_a1b_mem schema,
# runs a fresh forge-worker until they settle, and samples its process tree.
set -uo pipefail
S=C:/Users/damon/AppData/Local/Temp/claude/C--Users-damon-Downloads-agents/9c73bcba-7500-4641-9433-f7c8efd05843/scratchpad
M=$S/mem
label=$1 pool=$2 conc=$3 size=$4 copies=$5
export GOWORK=off
export FORGE_DATABASE_URL='postgres://forge:forge_dev_pw@localhost:55840/forge?sslmode=disable&search_path=forge_a1b_mem'
export FORGE_LLM_API_KEY=stub FORGE_LLM_BASE_URL=http://127.0.0.1:55890/v1
export FORGE_LLM_CONVERSE_MODEL=stub-converse FORGE_LLM_VISION_MODEL=stub-vision FORGE_LLM_PLANNER_MODEL=stub-planner
export FORGE_LLM_EXECUTOR_MODEL=stub-executor FORGE_LLM_VERIFIER_MODEL=other-verifier
export FORGE_DATA_BOUNDARY=no_training
export FORGE_POLL_INTERVAL=500ms FORGE_WORKSPACE_ROOT=$M/ws
export FORGE_CAD_PYTHON=C:/Users/damon/Downloads/agents/jarvis-k2b/.cadvenv/Scripts/python.exe
export FORGE_CAD_POOL=$pool FORGE_WORKER_CONCURRENCY=$conc FORGE_ALLOW_SCRIPTS=false
psqlm() { docker exec forge-pg psql -U forge -d forge -Atc "set search_path=forge_a1b_mem; $1" | tail -n +2; }

cd $M || exit 1
for c in $(seq 1 $copies); do
  $S/bin/forgectl.exe goal new --build --title "mem $label #$c" --statement "a tree occurrences=$size ${SHAPE:-}" \
    --owner mem@a1b.local --start > $M/$label-new-$c.log 2>&1 || { echo "goal new failed"; cat $M/$label-new-$c.log; exit 1; }
done

t0=$(date +%s)
$S/bin/forge-worker.exe > $M/$label-worker.log 2>&1 &
bpid=$!
sleep 1
wpid=$(cat /proc/$bpid/winpid)
powershell -NoProfile -ExecutionPolicy Bypass -File "$(cygpath -w $M/sampler.ps1)" -RootPid $wpid -Out "$(cygpath -w $M/$label.csv)" &
spid=$!

deadline=$((t0 + 1800))
while :; do
  active=$(psqlm "select count(*) from forge_goals where title like 'mem $label #%' and status = 'active'")
  [ "$active" = "0" ] && break
  [ "$(date +%s)" -gt "$deadline" ] && { echo "TIMEOUT"; break; }
  sleep 2
done
secs=$(( $(date +%s) - t0 ))
sleep 2   # one more sample at rest
taskkill //PID $wpid //T //F > /dev/null 2>&1
wait $spid 2>/dev/null
echo "== $label pool=$pool conc=$conc occurrences=$size copies=$copies seconds=$secs"
psqlm "select g.title, g.status, g.tokens_spent, extract(epoch from g.ended_at - g.started_at)::int from forge_goals g where g.title like 'mem $label #%' order by 1"
psqlm "select t.title, t.status, coalesce(t.error_code,''), left(coalesce(t.result::text, t.error_detail, ''), 260) from forge_tasks t join forge_goals g on g.id = t.goal_id where g.title like 'mem $label #%' order by g.title, t.idempotency_key"
