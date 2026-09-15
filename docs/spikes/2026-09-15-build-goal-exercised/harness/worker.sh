#!/usr/bin/env bash
# forge-worker from worktree A, started by ctrlrun so a real CTRL_BREAK_EVENT can stop it.
#   worker.sh <n>      creating $L/stop-<n> sends the stop; the worker logs to worker-<n>.log
. C:/Users/damon/AppData/Local/Temp/claude/C--Users-damon-Downloads-agents/9c73bcba-7500-4641-9433-f7c8efd05843/scratchpad/livegoal/env.sh
mkdir -p $L/ws $L/run-worker && cd $L/run-worker || exit 1
export FORGE_WORKER_CONCURRENCY=1 FORGE_POLL_INTERVAL=2s
# A 60 s lease: a step handed back on a graceful stop is visibly faster than one
# left to expire, and a missed hand-back is still recovered within a minute.
export FORGE_LEASE_DURATION=60s FORGE_LEASE_HEARTBEAT=15s
export FORGE_WORKSPACE_ROOT=$L/ws
# Every model call through rec-proxy.js, which records the provider's usage even for a
# call the worker abandons when it stops (the proxy keeps reading the upstream reply).
export FORGE_LLM_BASE_URL=http://127.0.0.1:18122/compatible-mode/v1
exec $L/bin/ctrlrun.exe -log $L/worker-$1.log -trigger $L/stop-$1 -- $L/bin/forge-worker.exe >> $L/ctrlrun-$1.log 2>&1
