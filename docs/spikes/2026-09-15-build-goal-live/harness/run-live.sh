#!/usr/bin/env bash
# The live build-goal run (A1 follow-ups). Local database, production model endpoint.
# The key is loaded into THIS shell only, from jarvis-a4/.env, and never printed.
set -uo pipefail
S=C:/Users/damon/AppData/Local/Temp/claude/C--Users-damon-Downloads-agents/9c73bcba-7500-4641-9433-f7c8efd05843/scratchpad
L=$S/live
mkdir -p $L/ws
cd $L || exit 1

set -a; . /c/Users/damon/Downloads/agents/jarvis-a4/.env; set +a
export GOWORK=off
export FORGE_DATABASE_URL='postgres://forge:forge_dev_pw@localhost:55840/forge?sslmode=disable'
export FORGE_LLM_BASE_URL=https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode/v1
export FORGE_LLM_CONVERSE_MODEL=qwen3.7-plus FORGE_LLM_VISION_MODEL=qwen3.8-max FORGE_LLM_PLANNER_MODEL=qwen3.8-max
export FORGE_DATA_BOUNDARY=no_training
# Debug: every model call's prompt and completion tokens (forge.llm.completed).
export FORGE_LOG_LEVEL=debug
# The goal's ceiling, for the planning call too (forgectl charges it before the row can be set).
export FORGE_MAX_TOKENS_PER_GOAL=300000
export FORGE_WORKER_CONCURRENCY=1 FORGE_POLL_INTERVAL=2s
# A shorter lease, so the deliberate stop below is recovered in a minute rather than two.
export FORGE_LEASE_DURATION=60s FORGE_LEASE_HEARTBEAT=15s
export FORGE_WORKSPACE_ROOT=$L/ws
export FORGE_CAD_PYTHON=C:/Users/damon/Downloads/agents/jarvis-k2b/.cadvenv/Scripts/python.exe
export FORGE_CAD_POOL=1 FORGE_ALLOW_SCRIPTS=false

psq() { docker exec forge-pg psql -U forge -d forge -Atc "$1"; }
stamp() { date '+%H:%M:%S'; }

TITLE="A1b live build: sports car"
echo "$(stamp) planning" | tee -a $L/driver.log
$S/bin/forgectl.exe goal new --build --title "$TITLE" \
  --statement "a sports car, in as much mechanical detail as you can manage" \
  --owner a1b-live-build@forge.local --industry automotive --start > $L/goal-new.log 2>&1
rc=$?
echo "$(stamp) goal new exit=$rc" | tee -a $L/driver.log
GOAL=$(grep -o 'goal gol_[A-Za-z0-9]*' $L/goal-new.log | head -1 | cut -d' ' -f2)
[ -n "$GOAL" ] || { echo "no goal id"; cat $L/goal-new.log; exit 1; }
echo "$GOAL" > $L/goal.id
psq "update forge_goals set max_tokens = 300000 where id = '$GOAL'" | tee -a $L/driver.log
psq "select status, max_tokens, tokens_spent from forge_goals where id = '$GOAL'" | tee -a $L/driver.log
[ $rc -eq 0 ] || exit 1

HARD_STOP=255000   # the watchdog: one step call can overshoot the ceiling by tens of thousands
start_worker() {
  $S/bin/forge-worker.exe >> $L/worker-$1.log 2>&1 &
  WB=$!; sleep 1; WPID=$(cat /proc/$WB/winpid)
  echo "$(stamp) worker $1 started winpid=$WPID" | tee -a $L/driver.log
}
stop_worker() {
  taskkill //PID $WPID //T //F > /dev/null 2>&1
  echo "$(stamp) worker stopped ($1) winpid=$WPID" | tee -a $L/driver.log
}

start_worker 1
stopped_once=0 last=""
while :; do
  row=$(psq "select status, tokens_spent from forge_goals where id = '$GOAL'")
  status=${row%%|*} spent=${row##*|}
  tasks=$(psq "select string_agg(right(idempotency_key,2)||':'||status||':'||attempt_count, ' ' order by idempotency_key) from forge_tasks where goal_id = '$GOAL'")
  line="status=$status spent=$spent tasks=[$tasks]"
  [ "$line" != "$last" ] && { echo "$(stamp) $line" | tee -a $L/progress.log; last=$line; }
  [ "$status" != "active" ] && break
  if [ "${spent:-0}" -ge $HARD_STOP ]; then
    stop_worker "watchdog: spent $spent >= $HARD_STOP"; echo "WATCHDOG" | tee -a $L/driver.log; break
  fi
  # The deliberate stop: once two steps are kept and the third has been running
  # for 45 seconds, kill the worker (as a pod kill would) and start a fresh one.
  if [ $stopped_once -eq 0 ]; then
    ready=$(psq "select count(*) from forge_tasks where goal_id = '$GOAL' and status = 'succeeded'")
    mid=$(psq "select count(*) from forge_tasks where goal_id = '$GOAL' and status = 'running' and started_at < now() - interval '45 seconds'")
    if [ "${ready:-0}" -ge 2 ] && [ "${mid:-0}" -ge 1 ]; then
      psq "select idempotency_key, status, attempt_count from forge_tasks where goal_id = '$GOAL' and status = 'running'" | tee -a $L/driver.log
      echo "$(stamp) spent before stop: $spent" | tee -a $L/driver.log
      stop_worker "deliberate mid-step stop"
      stopped_once=1
      sleep 5
      start_worker 2
    fi
  fi
  sleep 5
done
stop_worker "end"
echo "$(stamp) done" | tee -a $L/driver.log
