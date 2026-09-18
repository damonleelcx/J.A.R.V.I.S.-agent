#!/usr/bin/env bash
# usage: omb_forged.sh <exe> <port> [proxyport]  forged against the harness's Postgres, MinIO and kernel
W=/c/Users/damon/Downloads/agents/J.A.R.V.I.S.-agent/.claude/worktrees/agent-a1fdd05834724cdd8
. $W/docs/spikes/2026-09-17-workbench-viewport/harness/env.sh
V=C:/Users/damon/AppData/Local/Temp/claude/C--Users-damon-Downloads-agents/9c73bcba-7500-4641-9433-f7c8efd05843/scratchpad/vplarge
mkdir -p $V/omb-run-$2 $V/outbox && cd $V/omb-run-$2 || exit 1
export FORGE_HTTP_ADDR=127.0.0.1:$2 FORGE_PUBLIC_URL=http://127.0.0.1:${3:-$2}
export FORGE_SESSION_SECRET="$(cat $L/secret.txt)" FORGE_MAIL_OUTBOX_DIR=$V/outbox
export FORGE_GEOMETRY_MAX_OCCURRENCES=1100000
exec timeout 7200 $1 >> $V/omb-forged-$2.log 2>&1
