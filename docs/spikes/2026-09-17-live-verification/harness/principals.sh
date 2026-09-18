#!/usr/bin/env bash
. /c/Users/damon/AppData/Local/Temp/claude/C--Users-damon-Downloads-agents/9c73bcba-7500-4641-9433-f7c8efd05843/scratchpad/lv/env.sh
cd /c/Users/damon/Downloads/agents/J.A.R.V.I.S.-agent/.claude/worktrees/agent-a325f3417a864b26b || exit 1
timeout 600 go run docs/spikes/2026-09-17-unverified-paths/harness/principals/main.go -out $L/principals.json -design $L/car-run1.json
echo "exit=$?"
