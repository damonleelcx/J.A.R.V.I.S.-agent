#!/usr/bin/env bash
# forge-worker for the off-node STEP export job, with its own CAD kernel.
. "$(dirname "$0")/env.sh"
mkdir -p $L/ws $L/run-worker && cd $L/run-worker || exit 1
export FORGE_WORKER_CONCURRENCY=1 FORGE_POLL_INTERVAL=2s FORGE_WORKSPACE_ROOT=$L/ws
exec $L/bin/forge-worker.exe >> $L/worker.log 2>&1
