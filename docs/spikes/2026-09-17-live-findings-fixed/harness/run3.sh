#!/usr/bin/env bash
# Run 3: forged + forge-worker locally, real model, one build goal driven over HTTP.
# The key is loaded into THIS shell only, from jarvis-a4/.env, and never printed.
set -uo pipefail
. /c/Users/damon/AppData/Local/Temp/claude/C--Users-damon-Downloads-agents/9c73bcba-7500-4641-9433-f7c8efd05843/scratchpad/lv2/env.sh
set -a; . /c/Users/damon/Downloads/agents/jarvis-a4/.env; set +a
export FORGE_LLM_BASE_URL=https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode/v1
export FORGE_SESSION_SECRET=$(openssl rand -hex 32)
export FORGE_WORKER_CONCURRENCY=1 FORGE_POLL_INTERVAL=2s FORGE_WORKSPACE_ROOT=$L/ws
mkdir -p $L/ws
cd $L || exit 1
date '+start %H:%M:%S'
$L/bin/forged.exe > $L/forged.log 2>&1 &
FB=$!
ok=0
for i in $(seq 1 60); do
  if curl -fsS http://127.0.0.1:18481/healthz > /dev/null 2>&1; then ok=1; break; fi
  kill -0 $FB 2>/dev/null || break
  sleep 1
done
[ $ok -eq 1 ] || { echo "forged did not come up"; tail -20 $L/forged.log; kill $FB; exit 1; }
FPID=$(cat /proc/$FB/winpid)
$L/bin/forge-worker.exe > $L/worker.log 2>&1 &
WB=$!
sleep 1
cat /proc/$WB/winpid > $L/worker.pid
echo "forged winpid=$FPID worker winpid=$(cat $L/worker.pid)"
timeout 2400 python $L/run3.py
echo "driver exit=$?"
taskkill //PID $(cat $L/worker.pid) //T //F > /dev/null 2>&1
taskkill //PID $FPID //T //F > /dev/null 2>&1
date '+end %H:%M:%S'
