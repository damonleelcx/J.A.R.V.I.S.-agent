#!/usr/bin/env bash
# forged from agent/build-goal-entry (worktree A) on 127.0.0.1:18120; the pane reaches it via proxy.js on 18121.
. C:/Users/damon/AppData/Local/Temp/claude/C--Users-damon-Downloads-agents/9c73bcba-7500-4641-9433-f7c8efd05843/scratchpad/livegoal/env.sh
mkdir -p $L/run-forged && cd $L/run-forged || exit 1
export FORGE_HTTP_ADDR=127.0.0.1:18120 FORGE_PUBLIC_URL=http://127.0.0.1:18121
export FORGE_SESSION_SECRET="$(cat $L/secret.txt)"
export FORGE_MAIL_OUTBOX_DIR=$L/outbox
# LLM_URL points forged at rec-proxy.js to capture a turn's model reply.
[ -n "$LLM_URL" ] && export FORGE_LLM_BASE_URL="$LLM_URL"
exec $L/bin/forged.exe >> $L/forged.log 2>&1
