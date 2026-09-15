#!/usr/bin/env bash
# forged from viewport/subtree-loading (worktree B binary) on 127.0.0.1:18130 for the hidden-pane
# measurement; the pane reaches it through proxy.js on 18131. No conversation is held: the model
# endpoint is an unused local port and the key is a placeholder, so no token can be spent.
V=C:/Users/damon/AppData/Local/Temp/claude/C--Users-damon-Downloads-agents/9c73bcba-7500-4641-9433-f7c8efd05843/scratchpad/viewport
L=C:/Users/damon/AppData/Local/Temp/claude/C--Users-damon-Downloads-agents/9c73bcba-7500-4641-9433-f7c8efd05843/scratchpad/livegoal
mkdir -p $V/run-forged && cd $V/run-forged || exit 1
export GOWORK=off FORGE_ENV=development
export FORGE_DATABASE_URL='postgres://forge:forge_dev_pw@localhost:55840/forge?sslmode=disable'
export FORGE_HTTP_ADDR=127.0.0.1:18130 FORGE_PUBLIC_URL=http://127.0.0.1:18131
export FORGE_SESSION_SECRET="$(cat $L/secret.txt)"
export FORGE_MAIL_OUTBOX_DIR=$V/outbox
export FORGE_LLM_BASE_URL=http://127.0.0.1:18139/v1 FORGE_LLM_API_KEY=placeholder-no-model-here
export FORGE_DATA_BOUNDARY=no_training FORGE_LOG_LEVEL=info
export FORGE_CAD_PYTHON=C:/Users/damon/Downloads/agents/jarvis-k2b/.cadvenv/Scripts/python.exe
export FORGE_CAD_POOL=1 FORGE_ALLOW_SCRIPTS=false
exec $V/bin/forged.exe >> $V/forged.log 2>&1
