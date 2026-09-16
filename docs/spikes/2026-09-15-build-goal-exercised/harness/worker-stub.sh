#!/usr/bin/env bash
# forge-worker from worktree A against stub-model.js (no live tokens, no real key), started by
# ctrlrun so a real CTRL_BREAK_EVENT can stop it. Same database, kernel and lease as worker.sh.
#   worker-stub.sh <n>     creating $L/stop-<n> sends the stop; logs to worker-<n>.log
L=C:/Users/damon/AppData/Local/Temp/claude/C--Users-damon-Downloads-agents/9c73bcba-7500-4641-9433-f7c8efd05843/scratchpad/livegoal
export GOWORK=off FORGE_ENV=development
export FORGE_DATABASE_URL='postgres://forge:forge_dev_pw@localhost:55840/forge?sslmode=disable'
export FORGE_LLM_BASE_URL=http://127.0.0.1:18123/v1 FORGE_LLM_API_KEY=stub-not-a-key
export FORGE_LLM_CONVERSE_MODEL=qwen3.7-plus FORGE_LLM_VISION_MODEL=qwen3.8-max FORGE_LLM_PLANNER_MODEL=qwen3.8-max
export FORGE_DATA_BOUNDARY=no_training FORGE_LOG_LEVEL=debug FORGE_MAX_TOKENS_PER_GOAL=100000
export FORGE_CAD_PYTHON=C:/Users/damon/Downloads/agents/jarvis-k2b/.cadvenv/Scripts/python.exe
export FORGE_CAD_POOL=1 FORGE_ALLOW_SCRIPTS=false
export FORGE_WORKER_CONCURRENCY=1 FORGE_POLL_INTERVAL=2s
export FORGE_LEASE_DURATION=60s FORGE_LEASE_HEARTBEAT=15s
export FORGE_WORKSPACE_ROOT=$L/ws
mkdir -p $L/ws $L/run-worker && cd $L/run-worker || exit 1
exec $L/bin/ctrlrun.exe -log $L/worker-$1.log -trigger $L/stop-$1 -- $L/bin/forge-worker.exe >> $L/ctrlrun-$1.log 2>&1
