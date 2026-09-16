#!/usr/bin/env bash
# forge-worker from jarvis-cardcheck under ctrlrun (creating $L/stop-<n> stops it gracefully).
. C:/Users/damon/AppData/Local/Temp/claude/C--Users-damon-Downloads-agents/9c73bcba-7500-4641-9433-f7c8efd05843/scratchpad/cardcheck/env.sh
mkdir -p $L/ws $L/run-worker && cd $L/run-worker || exit 1
export FORGE_WORKER_CONCURRENCY=1 FORGE_POLL_INTERVAL=2s
export FORGE_LEASE_DURATION=60s FORGE_LEASE_HEARTBEAT=15s
export FORGE_WORKSPACE_ROOT=$L/ws
exec $L/bin/ctrlrun.exe -log $L/worker-$1.log -trigger $L/stop-$1 -- $L/bin/forge-worker.exe >> $L/ctrlrun-$1.log 2>&1
